cmake_minimum_required(VERSION 3.16)
project(teslcam_camera_scorer CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(OpenCV REQUIRED COMPONENTS core imgproc)

add_executable(teslcam-camera-scorer main.cpp)
target_include_directories(teslcam-camera-scorer PRIVATE ${OpenCV_INCLUDE_DIRS})
target_link_libraries(teslcam-camera-scorer PRIVATE opencv_core opencv_imgproc)
